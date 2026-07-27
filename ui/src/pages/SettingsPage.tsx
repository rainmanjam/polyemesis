import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import {
  Check,
  Copy,
  Download,
  ExternalLink,
  KeyRound,
  Loader2,
  Save,
  ShieldCheck,
  Trash2,
  Unplug,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PageHeader } from "@/components/AppLayout";
// The capability matrix lives beside the destination dialog that renders it
// inline; this page shows the same rows as a full table. It should move to
// ui/src/lib/ when someone owns that file — it is data, not a component.
import {
  CAPABILITY_COLUMNS,
  PLATFORM_CAPABILITIES,
  SUPPORT_LEGEND,
  supportInfo,
  supportOf,
  tierInfo,
} from "@/components/DestinationDialog";
import { api } from "@/lib/api";
import { timestamp } from "@/lib/format";
import { toneBadge, toneText, type SignalTone } from "@/lib/signal";
import { PULL_SCHEMES, RTSP_TRANSPORTS } from "@/lib/types";
import type {
  ApiToken,
  CertInfo,
  IngestMode,
  PlatformAccount,
  PlatformCreds,
  Settings,
  SetupGuide,
  SystemInfo,
  TlsMode,
  TlsStatus,
} from "@/lib/types";

export function SettingsPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "ingest";

  const [settings, setSettings] = useState<Settings | null>(null);
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api.getSettings().then(setSettings).catch(() => {});
    api.system().then(setSystem).catch(() => {});
  }, []);

  // The OAuth round trip is a full navigation, so its outcome comes back in
  // the query string rather than as an XHR response.
  useEffect(() => {
    const ok = params.get("oauth_ok");
    const err = params.get("oauth_error");
    if (ok) toast.success(ok);
    if (err) toast.error(err);
    if (ok || err) {
      const next = new URLSearchParams(params);
      next.delete("oauth_ok");
      next.delete("oauth_error");
      setParams(next, { replace: true });
    }
  }, [params, setParams]);

  const save = async (next: Settings) => {
    setSaving(true);
    try {
      setSettings(await api.putSettings(next));
      toast.success("Settings saved. Affected processes have been restarted.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the settings.");
    } finally {
      setSaving(false);
    }
  };

  if (!settings) {
    return (
      <div className="flex h-full items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="p-3">
      <PageHeader title="Settings" subtitle={system ? `polyemesis ${system.version}` : undefined} />

      <Tabs value={tab} onValueChange={(v) => setParams({ tab: v })}>
        <TabsList>
          <TabsTrigger value="ingest">Ingest</TabsTrigger>
          <TabsTrigger value="pipeline">Pipeline</TabsTrigger>
          <TabsTrigger value="platforms">Platform credentials</TabsTrigger>
          <TabsTrigger value="security">Security</TabsTrigger>
        </TabsList>

        <TabsContent value="ingest">
          <IngestSettings settings={settings} system={system} onSave={save} saving={saving} />
        </TabsContent>
        <TabsContent value="pipeline">
          <PipelineSettings settings={settings} onSave={save} saving={saving} />
        </TabsContent>
        <TabsContent value="platforms">
          <PlatformSettings />
        </TabsContent>
        <TabsContent value="security">
          <SecuritySettings system={system} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/* ------------------------------------------------------------------ ingest */

function IngestSettings({
  settings,
  system,
  onSave,
  saving,
}: {
  settings: Settings;
  system: SystemInfo | null;
  onSave: (s: Settings) => void;
  saving: boolean;
}) {
  const [draft, setDraft] = useState(settings);
  useEffect(() => setDraft(settings), [settings]);

  const copyUrl = async () => {
    if (!system?.ingestUrl) return;
    await navigator.clipboard.writeText(system.ingestUrl);
    toast.success("Ingest URL copied.");
  };

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Ingest</CardTitle>
          <CardDescription>
            SRT carries up to six audio tracks. RTMP carries one, and exists as a fallback for
            encoders that cannot do SRT. Pull dials a source instead of waiting for one.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label>Mode</Label>
            <Select
              value={draft.ingest.mode}
              onValueChange={(v) =>
                setDraft({ ...draft, ingest: { ...draft.ingest, mode: v as IngestMode } })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="srt">SRT — multitrack (recommended)</SelectItem>
                <SelectItem value="rtmp">RTMP — single track</SelectItem>
                <SelectItem value="pull">Pull — dial a camera, feed or file</SelectItem>
              </SelectContent>
            </Select>
            {system && !system.ffmpeg.hasLibsrt && draft.ingest.mode === "srt" && (
              <span className="text-[10px] text-warn">
                This FFmpeg build has no SRT support. Install one configured with
                --enable-libsrt, or use RTMP.
              </span>
            )}
          </div>

          {draft.ingest.mode === "pull" ? (
            <PullIngestFields draft={draft} setDraft={setDraft} />
          ) : draft.ingest.mode === "srt" ? (
            <>
              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="srt-port">Port</Label>
                  <Input
                    id="srt-port"
                    type="number"
                    value={draft.ingest.srt.port}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          srt: { ...draft.ingest.srt, port: Number(e.target.value) },
                        },
                      })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="srt-latency">Latency (ms)</Label>
                  <Input
                    id="srt-latency"
                    type="number"
                    value={draft.ingest.srt.latencyMs}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          srt: { ...draft.ingest.srt, latencyMs: Number(e.target.value) },
                        },
                      })
                    }
                  />
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="srt-pass">Passphrase</Label>
                <Input
                  id="srt-pass"
                  type="password"
                  value={draft.ingest.srt.passphrase}
                  placeholder="optional — 10 to 79 characters"
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      ingest: {
                        ...draft.ingest,
                        srt: { ...draft.ingest.srt, passphrase: e.target.value },
                      },
                    })
                  }
                />
                <span className="text-[10px] text-muted-foreground">
                  Enables AES encryption. Leave blank for none. SRT rejects passphrases outside
                  10–79 characters.
                </span>
              </div>
            </>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="rtmp-port">Port</Label>
                  <Input
                    id="rtmp-port"
                    type="number"
                    value={draft.ingest.rtmp.port}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          rtmp: { ...draft.ingest.rtmp, port: Number(e.target.value) },
                        },
                      })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="rtmp-app">App</Label>
                  <Input
                    id="rtmp-app"
                    value={draft.ingest.rtmp.app}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          rtmp: { ...draft.ingest.rtmp, app: e.target.value },
                        },
                      })
                    }
                  />
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rtmp-key">Stream key</Label>
                <Input
                  id="rtmp-key"
                  value={draft.ingest.rtmp.streamKey}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      ingest: {
                        ...draft.ingest,
                        rtmp: { ...draft.ingest.rtmp, streamKey: e.target.value },
                      },
                    })
                  }
                />
                <span className="text-[10px] text-muted-foreground">
                  Publishers must use this exact path, so an open port is not an open door.
                </span>
              </div>
            </>
          )}

          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} Save ingest settings
          </Button>
        </CardContent>
      </Card>

      <Card className="h-fit">
        <CardHeader>
          <CardTitle>Encoder URL</CardTitle>
          <CardDescription>Point OBS at this address.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
            <code className="min-w-0 flex-1 break-all font-mono text-[10px] text-muted-foreground">
              {system?.ingestUrl ?? "…"}
            </code>
            <Button variant="ghost" size="icon-sm" onClick={copyUrl} aria-label="Copy">
              <Copy />
            </Button>
          </div>
          <p className="text-[10px] text-muted-foreground">
            For multitrack, use OBS → Settings → Output → Recording, set type to Custom Output
            (FFmpeg), container <code className="font-mono">mpegts</code>, and enable the audio
            tracks you want to send. The README has the exact field values.
          </p>
          {system && (
            <div className="mt-1 flex flex-wrap gap-1">
              <Badge variant="outline">ffmpeg {system.ffmpeg.version}</Badge>
              <Badge variant={system.ffmpeg.hasLibsrt ? "live" : "warn"}>
                srt {system.ffmpeg.hasLibsrt ? "yes" : "no"}
              </Badge>
              <Badge variant={system.ffmpeg.hasLibx264 ? "outline" : "warn"}>
                x264 {system.ffmpeg.hasLibx264 ? "yes" : "no"}
              </Badge>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** The pull source's fields.
 *
 *  The scheme list and the reconnect bounds are the SAME ones the server
 *  enforces, quoted here so a mistake is a hint under the field rather than a
 *  rejected save. They are a hint and nothing more: the server validates
 *  independently, because a UI check is a convenience and never a control. */
function PullIngestFields({
  draft,
  setDraft,
}: {
  draft: Settings;
  setDraft: (s: Settings) => void;
}) {
  const pull = draft.ingest.pull ?? { url: "", reconnectDelayMaxSeconds: 30, rtspTransport: "tcp" };
  const set = (patch: Partial<typeof pull>) =>
    setDraft({ ...draft, ingest: { ...draft.ingest, pull: { ...pull, ...patch } } });

  const scheme = pull.url.split("://")[0]?.toLowerCase() ?? "";
  const known = (PULL_SCHEMES as readonly string[]).includes(scheme);

  return (
    <>
      <div className="flex flex-col gap-1">
        <Label htmlFor="pull-url">Source URL</Label>
        <Input
          id="pull-url"
          value={pull.url}
          placeholder="rtsp://camera.local/stream1"
          onChange={(e) => set({ url: e.target.value })}
        />
        {pull.url !== "" && !known ? (
          <span className="text-[10px] text-warn">
            The server dials only: {PULL_SCHEMES.join(", ")}.
          </span>
        ) : (
          <span className="text-[10px] text-muted-foreground">
            One of {PULL_SCHEMES.join(", ")}. A file:// source is a path RELATIVE to the data
            directory and may not contain ".." — it is confined there the same way a file
            destination is.
          </span>
        )}
      </div>

      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="pull-reconnect">Reconnect backoff cap (s)</Label>
          <Input
            id="pull-reconnect"
            type="number"
            value={pull.reconnectDelayMaxSeconds}
            onChange={(e) => set({ reconnectDelayMaxSeconds: Number(e.target.value) })}
          />
          <span className="text-[10px] text-muted-foreground">
            0 uses the default. HTTP and HLS sources only.
          </span>
        </div>
        <div className="flex flex-col gap-1">
          <Label>RTSP transport</Label>
          <Select value={pull.rtspTransport || "tcp"} onValueChange={(v) => set({ rtspTransport: v })}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RTSP_TRANSPORTS.map((t) => (
                <SelectItem key={t} value={t}>
                  {t}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-[10px] text-muted-foreground">
            TCP unless the camera cannot. RTSP over UDP through NAT looks connected and delivers
            nothing.
          </span>
        </div>
      </div>
    </>
  );
}

/* ---------------------------------------------------------------- pipeline */

function PipelineSettings({
  settings,
  onSave,
  saving,
}: {
  settings: Settings;
  onSave: (s: Settings) => void;
  saving: boolean;
}) {
  const [draft, setDraft] = useState(settings);
  useEffect(() => setDraft(settings), [settings]);

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Synthetic audio</CardTitle>
          <CardDescription>
            What happens when the ingest carries video and no audio at all.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="synth-silence">Synthesise a silent track</Label>
            <Switch
              id="synth-silence"
              checked={draft.synth?.silenceOnVideoOnly ?? true}
              onCheckedChange={(v) => setDraft({ ...draft, synth: { silenceOnVideoOnly: v } })}
            />
          </div>
          <span className="text-[10px] text-muted-foreground">
            Every major platform rejects a stream with no audio track. With this on, a video-only
            ingest gains one silent stereo track and its destinations start; with it off they refuse
            with "routing profile selects no audio". It only ever applies to an ingest that PROBED
            with zero tracks, so it can never displace audio you are actually sending.
          </span>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} Save
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Preview</CardTitle>
          <CardDescription>
            The dashboard's HLS monitor. This is the only place polyemesis re-encodes video, and it
            runs on its own relay subscription so it can never affect a destination.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="prev-enabled">Enabled</Label>
            <Switch
              id="prev-enabled"
              checked={draft.preview.enabled}
              onCheckedChange={(v) => setDraft({ ...draft, preview: { ...draft.preview, enabled: v } })}
            />
          </div>
          <div className="grid grid-cols-3 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="prev-seg">Segment (s)</Label>
              <Input
                id="prev-seg"
                type="number"
                min={1}
                max={10}
                value={draft.preview.segmentSeconds}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    preview: { ...draft.preview, segmentSeconds: Number(e.target.value) },
                  })
                }
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="prev-h">Height</Label>
              <Input
                id="prev-h"
                type="number"
                value={draft.preview.videoHeight}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    preview: { ...draft.preview, videoHeight: Number(e.target.value) },
                  })
                }
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="prev-kbps">kbps</Label>
              <Input
                id="prev-kbps"
                type="number"
                value={draft.preview.videoKbps}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    preview: { ...draft.preview, videoKbps: Number(e.target.value) },
                  })
                }
              />
            </div>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="prev-idle">Stop after idle (s)</Label>
            <Input
              id="prev-idle"
              type="number"
              min={5}
              max={3600}
              value={draft.preview.idleTimeoutSeconds}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  preview: { ...draft.preview, idleTimeoutSeconds: Number(e.target.value) },
                })
              }
            />
            <span className="text-[10px] text-muted-foreground">
              The encoder starts when the dashboard asks for the playlist and stops again this long
              after the last request, so a closed dashboard costs no CPU. Changing this never
              restarts a preview someone is watching.
            </span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Audio meters</CardTitle>
          <CardDescription>
            One sidecar process meters every channel of every track. Lower intervals update faster
            and cost more CPU.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="met-enabled">Enabled</Label>
            <Switch
              id="met-enabled"
              checked={draft.meters.enabled}
              onCheckedChange={(v) => setDraft({ ...draft, meters: { ...draft.meters, enabled: v } })}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="met-int">Update interval (ms)</Label>
            <Input
              id="met-int"
              type="number"
              min={40}
              max={2000}
              value={draft.meters.intervalMs}
              onChange={(e) =>
                setDraft({ ...draft, meters: { ...draft.meters, intervalMs: Number(e.target.value) } })
              }
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Process logs</CardTitle>
          <CardDescription>
            Captured FFmpeg stderr is always kept in memory, but that dies with the server — which
            loses exactly the lines explaining why it died. Persisting mirrors it to a rotating
            file under the data directory.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="log-persist">Write logs to disk</Label>
            <Switch
              id="log-persist"
              checked={draft.logging.persistProcessLogs}
              onCheckedChange={(v) =>
                setDraft({ ...draft, logging: { ...draft.logging, persistProcessLogs: v } })
              }
            />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="log-mb">File size (MB)</Label>
              <Input
                id="log-mb"
                type="number"
                min={1}
                max={1024}
                value={draft.logging.maxFileMb}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    logging: { ...draft.logging, maxFileMb: Number(e.target.value) },
                  })
                }
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="log-files">Files kept</Label>
              <Input
                id="log-files"
                type="number"
                min={1}
                max={100}
                value={draft.logging.maxFiles}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    logging: { ...draft.logging, maxFiles: Number(e.target.value) },
                  })
                }
              />
            </div>
          </div>
          <span className="text-[10px] text-muted-foreground">
            The log directory is bounded by the product of these two, so persistence can never be
            the thing that fills the disk.
          </span>
        </CardContent>
      </Card>

      <div className="lg:col-span-2">
        <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />} Save pipeline settings
        </Button>
      </div>
    </div>
  );
}

/* --------------------------------------------------------------- platforms */

function PlatformSettings() {
  const [guides, setGuides] = useState<SetupGuide[]>([]);
  const [creds, setCreds] = useState<PlatformCreds[]>([]);
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);

  const load = () => {
    api.platformGuides().then(setGuides).catch(() => {});
    api.listCreds().then(setCreds).catch(() => {});
    api.listAccounts().then(setAccounts).catch(() => {});
  };
  useEffect(load, []);

  return (
    <div className="flex flex-col gap-3">
      <PlatformCapabilityMatrix />

      <Card>
        <CardHeader>
          <CardTitle>Why you need your own developer app</CardTitle>
          <CardDescription>
            polyemesis cannot ship OAuth client secrets — anyone with the binary would have them,
            and the platforms would revoke them. You register a free developer app once, paste the
            client ID and secret here, and polyemesis then fetches your stream keys for you.
          </CardDescription>
        </CardHeader>
      </Card>

      {guides.map((g) => (
        <PlatformCredCard
          key={g.platform}
          guide={g}
          creds={creds.find((c) => c.platform === g.platform) ?? null}
          accounts={accounts.filter((a) => a.platform === g.platform)}
          onChanged={load}
        />
      ))}
    </div>
  );
}

/** The honest answer to "what do I actually get?", above the setup forms
 *  rather than below them.
 *
 *  Every row of this table was a question somebody would otherwise only answer
 *  by spending an evening: Kick signs in but never hands over a key; Facebook
 *  works completely and needs Meta's App Review first; Instagram cannot be
 *  streamed to at all. Reading it takes thirty seconds and it is deliberately
 *  the first thing on this tab. */
function PlatformCapabilityMatrix() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>What each platform can do</CardTitle>
        <CardDescription>
          Platforms differ in how much of this polyemesis can automate, and the difference is
          worth knowing before you start rather than an hour in. Streaming itself works the same
          everywhere — per-destination audio routing, renditions, reconnect and meters do not
          depend on any of the columns below.
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Platform</TableHead>
                {CAPABILITY_COLUMNS.map((c) => (
                  <TableHead key={c.key} title={c.help}>
                    {c.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {PLATFORM_CAPABILITIES.map((p) => (
                <TableRow key={p.presetId}>
                  <TableCell>
                    <div className="flex flex-col gap-0.5">
                      <span
                        className={
                          p.tier === "unsupported" ? "text-muted-foreground" : undefined
                        }
                      >
                        {p.name}
                      </span>
                      <Badge
                        variant={p.tier === "unsupported" ? "warn" : "outline"}
                        className="w-fit"
                        title={tierInfo(p.tier).help}
                      >
                        {tierInfo(p.tier).label}
                      </Badge>
                    </div>
                  </TableCell>
                  {CAPABILITY_COLUMNS.map((c) => {
                    const info = supportInfo(supportOf(p, c.key));
                    return (
                      <TableCell key={c.key}>
                        {/* The reason, where there is one, is the cell's own
                            tooltip: a tick with no explanation is how a claim
                            becomes folklore. */}
                        <Badge
                          variant={info.variant}
                          className="normal-case"
                          title={p.reasons?.[c.key] ?? info.help}
                        >
                          {info.label}
                        </Badge>
                      </TableCell>
                    );
                  })}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>

        <div className="flex flex-wrap gap-x-3 gap-y-1">
          {SUPPORT_LEGEND.map((l) => (
            <span key={l.key} className="flex items-center gap-1.5">
              <Badge variant={l.variant} className="normal-case">
                {l.label}
              </Badge>
              <span className="text-[10px] text-muted-foreground">{l.help}</span>
            </span>
          ))}
        </div>

        {/* "Unverified" is the load-bearing value here and the one most likely
            to be misread as "no". Saying it plainly costs a sentence and saves
            an operator concluding a feature is blocked when it was only never
            checked. */}
        <p className="text-[10px] text-muted-foreground">
          Unverified means exactly that: polyemesis does not do it today and nobody here has read
          the platform's API either way. It is never a refusal — if you can make it work, it
          works, and we would rather be told than guess. Only “not possible” is a checked
          absence.
        </p>

        {PLATFORM_CAPABILITIES.filter((p) => p.readFirst).map((p) => (
          <div
            key={p.presetId}
            className={
              p.tier === "unsupported"
                ? "flex flex-col gap-1 rounded-md border border-warn/40 bg-warn-dim p-2"
                : "flex flex-col gap-1 rounded-md border border-dashed p-2"
            }
          >
            <div className="flex items-center gap-1.5">
              <span className="text-[11px] font-medium">{p.name}</span>
              <Badge variant={p.tier === "unsupported" ? "warn" : "outline"}>read first</Badge>
            </div>
            <p
              className={
                p.tier === "unsupported" ? "text-[10px] text-warn" : "text-[10px] text-muted-foreground"
              }
            >
              {p.readFirst}
            </p>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function PlatformCredCard({
  guide,
  creds,
  accounts,
  onChanged,
}: {
  guide: SetupGuide;
  creds: PlatformCreds | null;
  accounts: PlatformAccount[];
  onChanged: () => void;
}) {
  const [clientId, setClientId] = useState(creds?.clientId ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => setClientId(creds?.clientId ?? ""), [creds]);

  const save = async () => {
    setBusy(true);
    try {
      await api.putCreds(guide.platform, clientId.trim(), clientSecret.trim());
      toast.success(`${guide.name} credentials saved.`);
      setClientSecret("");
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the credentials.");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!window.confirm(`Remove the ${guide.name} developer credentials?`)) return;
    await api.deleteCreds(guide.platform);
    toast.success("Credentials removed.");
    onChanged();
  };

  const disconnect = async (a: PlatformAccount) => {
    if (!window.confirm(`Disconnect ${a.accountName}?`)) return;
    await api.deleteAccount(a.id);
    toast.success("Account disconnected.");
    onChanged();
  };

  const copyRedirect = async () => {
    await navigator.clipboard.writeText(guide.redirectPath);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle className="flex items-center gap-2">
            {guide.name}
            {!guide.supported && <Badge variant="outline">manual key</Badge>}
            {creds?.hasSecret && <Badge variant="live">configured</Badge>}
          </CardTitle>
          {guide.note && <CardDescription className="mt-1">{guide.note}</CardDescription>}
        </div>
        {guide.consoleUrl && (
          <Button variant="outline" size="sm" asChild>
            <a href={guide.consoleUrl} target="_blank" rel="noreferrer noopener">
              <ExternalLink /> Console
            </a>
          </Button>
        )}
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <Accordion type="single" collapsible>
          <AccordionItem value="steps" className="border-b-0">
            <AccordionTrigger>Step-by-step setup</AccordionTrigger>
            <AccordionContent>
              <ol className="flex list-decimal flex-col gap-1.5 pl-4 text-[11px] text-muted-foreground">
                {guide.steps.map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ol>
              {guide.redirectPath && (
                <div className="mt-2 flex flex-col gap-1">
                  <Label>Redirect URI to whitelist</Label>
                  <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                    <code className="min-w-0 flex-1 break-all font-mono text-[10px]">
                      {guide.redirectPath}
                    </code>
                    <Button variant="ghost" size="icon-sm" onClick={copyRedirect} aria-label="Copy">
                      {copied ? <Check className="text-live" /> : <Copy />}
                    </Button>
                  </div>
                  <span className="text-[10px] text-muted-foreground">
                    Must match exactly, including scheme and port. If you reach polyemesis through a
                    reverse proxy, use that public URL.
                  </span>
                </div>
              )}
              {guide.scopes && guide.scopes.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {guide.scopes.map((s) => (
                    <Badge key={s} variant="outline" className="font-mono normal-case">
                      {s}
                    </Badge>
                  ))}
                </div>
              )}
            </AccordionContent>
          </AccordionItem>
        </Accordion>

        {guide.supported && (
          <>
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`cid-${guide.platform}`}>Client ID</Label>
                <Input
                  id={`cid-${guide.platform}`}
                  value={clientId}
                  onChange={(e) => setClientId(e.target.value)}
                  className="font-mono"
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`cs-${guide.platform}`}>Client secret</Label>
                <Input
                  id={`cs-${guide.platform}`}
                  type="password"
                  value={clientSecret}
                  placeholder={creds?.hasSecret ? "•••••••• (stored, encrypted)" : ""}
                  onChange={(e) => setClientSecret(e.target.value)}
                  className="font-mono"
                />
              </div>
            </div>

            <div className="flex flex-wrap gap-1.5">
              <Button size="sm" onClick={save} disabled={busy || !clientId.trim() || !clientSecret.trim()}>
                {busy ? <Loader2 className="animate-spin" /> : <Save />} Save credentials
              </Button>
              {creds && (
                <>
                  <Button size="sm" variant="outline" asChild>
                    <a href={api.connectUrl(guide.platform)}>
                      <ExternalLink /> Connect an account
                    </a>
                  </Button>
                  <Button size="sm" variant="ghost" onClick={remove}>
                    <Trash2 /> Remove
                  </Button>
                </>
              )}
            </div>

            {accounts.length > 0 && (
              <div className="flex flex-col gap-1">
                <Label>Connected accounts</Label>
                {accounts.map((a) => (
                  <div
                    key={a.id}
                    className="flex items-center justify-between rounded border border-border bg-background px-2 py-1.5"
                  >
                    <div className="min-w-0">
                      <div className="truncate text-[12px]">{a.accountName}</div>
                      <div className="font-mono text-[10px] text-muted-foreground">{a.accountRef}</div>
                    </div>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => disconnect(a)}
                      aria-label="Disconnect"
                    >
                      <Unplug />
                    </Button>
                  </div>
                ))}
                <span className="text-[10px] text-muted-foreground">
                  Connect the same platform more than once to stream to multiple channels — each
                  account becomes its own destination.
                </span>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

/* ---------------------------------------------------------------- security */

function SecuritySettings({ system }: { system: SystemInfo | null }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  const change = async (e: React.FormEvent) => {
    e.preventDefault();
    if (next !== confirm) {
      toast.error("The new passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api.changePassword(current, next);
      toast.success("Password changed.");
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not change the password.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Admin password</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={change} className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="cur-pw">Current password</Label>
              <Input
                id="cur-pw"
                type="password"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="new-pw">New password</Label>
              <Input
                id="new-pw"
                type="password"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="conf-pw">Confirm new password</Label>
              <Input
                id="conf-pw"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
              />
            </div>
            <Button type="submit" size="sm" disabled={busy}>
              {busy ? <Loader2 className="animate-spin" /> : <ShieldCheck />} Change password
            </Button>
          </form>
        </CardContent>
      </Card>

      <TransportSecurity system={system} />

      <ApiTokens />
    </div>
  );
}

/* ------------------------------------------------------- transport security */

/** Human labels for the resolved mode. `auto` never reaches here — the server
 *  resolves it — but it is listed so the map stays exhaustive over TlsMode. */
const tlsModeLabel: Record<TlsMode, string> = {
  auto: "Auto",
  acme: "Let's Encrypt",
  selfsigned: "Self-signed",
  manual: "Manual certificate",
  off: "Not terminated here",
};

/** Signal tone for the resolved mode. `off` is the one that depends on
 *  context: behind a trusted proxy someone else is doing TLS correctly, which
 *  is `armed`; without one the login form and session cookie cross the network
 *  in plaintext, which is exactly what `down` is for. */
function modeTone(tls: TlsStatus): SignalTone {
  switch (tls.mode) {
    case "acme":
    case "manual":
      return "live";
    case "selfsigned":
      return "warn";
    default:
      return tls.trustProxyHeaders ? "armed" : "down";
  }
}

/** Expiry is the number every operator actually watches. Thirty days is the
 *  point renewal should already have happened; seven is the point it has
 *  started going wrong. */
function expiryTone(daysRemaining: number): SignalTone {
  if (daysRemaining < 7) return "down";
  if (daysRemaining < 30) return "warn";
  return "live";
}

function expiryLabel(cert: CertInfo): string {
  if (cert.expired) {
    const ago = Math.abs(cert.daysRemaining);
    return ago === 0 ? "expired today" : `expired ${ago} day${ago === 1 ? "" : "s"} ago`;
  }
  return `${cert.daysRemaining} day${cert.daysRemaining === 1 ? "" : "s"} left`;
}

/** The config.yaml the current mode corresponds to, filled in with the values
 *  this server actually resolved so it is copy-pasteable rather than a sample. */
function tlsYaml(tls: TlsStatus): string {
  const host = tls.hostname || "streams.example.com";
  switch (tls.mode) {
    case "acme":
      return [
        "# config.yaml",
        "tls:",
        "  mode: acme",
        `  hostname: ${host}`,
        "  acmeEmail: you@example.com",
        "  hsts: true",
      ].join("\n");
    case "selfsigned":
      return [
        "# config.yaml",
        "tls:",
        "  mode: selfsigned",
        `  hostname: ${host}`,
        "  # hsts stays off: a browser must never be told to pin a host",
        "  # whose certificate it cannot validate.",
      ].join("\n");
    case "manual":
      return [
        "# config.yaml",
        "tls:",
        "  mode: manual",
        "  certFile: /etc/polyemesis/cert.pem",
        "  keyFile:  /etc/polyemesis/key.pem",
        "  hsts: true",
      ].join("\n");
    default:
      return [
        "# config.yaml",
        "tls:",
        "  mode: off",
        "# Set this when a reverse proxy terminates TLS in front of you, so",
        "# session cookies are marked Secure and OAuth uses your public origin.",
        "trustProxyHeaders: true",
      ].join("\n");
  }
}

function TransportSecurity({ system }: { system: SystemInfo | null }) {
  const [tls, setTls] = useState<TlsStatus | null>(null);
  const [loadError, setLoadError] = useState("");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    api
      .tlsStatus()
      .then(setTls)
      .catch((err) =>
        setLoadError(err instanceof Error ? err.message : "Could not read the TLS status."),
      );
  }, []);

  const copyYaml = async () => {
    if (!tls) return;
    await navigator.clipboard.writeText(tlsYaml(tls));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const cert = tls?.certificate ?? null;
  const sans = cert ? [...cert.dnsNames, ...cert.ipAddresses] : [];

  return (
    <Card className="h-fit">
      <CardHeader>
        <CardTitle>Transport security</CardTitle>
        <CardDescription>
          Configured in config.yaml, not here — TLS must be right before the server starts
          listening. This card reports what that produced.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {!tls && !loadError && (
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> Reading the certificate…
          </div>
        )}
        {loadError && <p className="text-[11px] text-down">{loadError}</p>}

        {tls && (
          <>
            <div className="flex items-center justify-between">
              <span className="text-[11px] text-muted-foreground">Mode</span>
              <div className="flex items-center gap-1.5">
                {tls.configured === "auto" && (
                  <span className="text-[10px] text-subtle-foreground">auto resolved to</span>
                )}
                <Badge variant={toneBadge[modeTone(tls)]}>{tlsModeLabel[tls.mode]}</Badge>
              </div>
            </div>

            {tls.mode === "off" && (
              <p className="text-[10px] text-muted-foreground">
                {tls.trustProxyHeaders
                  ? "A reverse proxy in front of polyemesis terminates TLS, so this server serves plain HTTP on purpose."
                  : "Nothing is encrypting this connection. Passwords and session cookies cross the network in plaintext — set tls.mode: auto, or bind to 127.0.0.1 behind a proxy."}
              </p>
            )}

            {cert ? (
              <>
                <div className="flex items-center justify-between gap-2 border-t border-border pt-2">
                  <span className="text-[11px] text-muted-foreground">Expires</span>
                  <span className="flex items-baseline gap-1.5">
                    <code className="tnum font-mono text-[10px]">{timestamp(cert.notAfter)}</code>
                    <span className={`tnum text-[10px] ${toneText[expiryTone(cert.daysRemaining)]}`}>
                      {expiryLabel(cert)}
                    </span>
                  </span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">Issuer</span>
                  <span className="min-w-0 break-all text-right text-[10px]">{cert.issuer}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">Subject</span>
                  <span className="min-w-0 break-all text-right text-[10px]">{cert.subject}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">Valid for</span>
                  <code className="min-w-0 break-all text-right font-mono text-[10px]">
                    {sans.length ? sans.join(", ") : "—"}
                  </code>
                </div>
              </>
            ) : (
              // In off mode the paragraph above already said this in context;
              // repeating the API's wording underneath it reads as a fault.
              tls.mode !== "off" &&
              tls.certificateError && (
                <p className="border-t border-border pt-2 text-[10px] text-muted-foreground">
                  {tls.certificateError}
                </p>
              )
            )}

            {tls.hstsWarning && <p className="text-[10px] text-warn">{tls.hstsWarning}</p>}
            {tls.hsts && (
              <p className="text-[10px] text-muted-foreground">
                HSTS is on. Browsers will refuse plain HTTP to
                <code className="mx-1 font-mono">{tls.hostname || "this host"}</code>
                until the max-age lapses; there is no server-side undo.
              </p>
            )}

            {tls.caAvailable && (
              <div className="flex flex-col gap-1.5 border-t border-border pt-2">
                <span className="text-[11px] text-muted-foreground">
                  Local certificate authority
                </span>
                <code className="tnum break-all rounded border border-border bg-background p-1.5 font-mono text-[9px] leading-relaxed text-muted-foreground">
                  {tls.caFingerprint}
                </code>
                {/* A plain link, not XHR: this is a file the browser saves, and
                    it stays reachable without a session so a user locked out by
                    the untrusted certificate can still get it. */}
                <Button asChild size="sm" variant="outline" className="w-fit">
                  <a href={api.caDownloadUrl()} download>
                    <Download /> Download CA certificate
                  </a>
                </Button>
                <p className="text-[10px] text-muted-foreground">
                  Install it to stop the browser warning, after checking the fingerprint above
                  matches the one your browser shows.
                  <span className="mt-1 block">
                    <strong className="font-medium text-foreground">macOS</strong> — open it in
                    Keychain Access, add to System, then set it to “Always Trust”.
                  </span>
                  <span className="block">
                    <strong className="font-medium text-foreground">Linux</strong> — copy to
                    <code className="mx-1 font-mono">/usr/local/share/ca-certificates/</code>and run
                    <code className="mx-1 font-mono">update-ca-certificates</code>.
                  </span>
                  <span className="block">
                    <strong className="font-medium text-foreground">Windows</strong> — import into
                    “Trusted Root Certification Authorities” for the local machine.
                  </span>
                  <span className="mt-1 block">
                    Firefox and Chrome keep their own stores on some platforms and may need it
                    imported there too.
                  </span>
                </p>
              </div>
            )}

            <div className="flex items-center justify-between border-t border-border pt-2">
              <span className="text-[11px] text-muted-foreground">config.yaml</span>
              <Button size="sm" variant="ghost" onClick={copyYaml}>
                {copied ? <Check /> : <Copy />} Copy
              </Button>
            </div>
            <pre className="overflow-x-auto rounded border border-border bg-background p-2 font-mono text-[10px] text-muted-foreground">
              {tlsYaml(tls)}
            </pre>
          </>
        )}

        <div className="flex items-start justify-between gap-2 border-t border-border pt-2">
          <span className="shrink-0 text-[11px] text-muted-foreground">Data directory</span>
          <code className="min-w-0 break-all text-right font-mono text-[10px]">
            {system?.dataDir ?? "—"}
          </code>
        </div>
        <p className="text-[10px] text-muted-foreground">
          OAuth tokens and client secrets are encrypted at rest with NaCl secretbox, keyed by
          <code className="mx-1 font-mono">secret.key</code> in that directory. Back it up with the
          database, or connected accounts must be re-authorised.
        </p>
      </CardContent>
    </Card>
  );
}

function ApiTokens() {
  const [tokens, setTokens] = useState<ApiToken[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  // Held in state and nowhere else: the server keeps only a hash, so this is
  // the one and only time the plaintext exists.
  const [minted, setMinted] = useState<{ token: ApiToken; plaintext: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const load = () => {
    api.listTokens().then(setTokens).catch(() => {});
  };
  useEffect(load, []);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      setMinted(await api.createToken(name.trim()));
      setCopied(false);
      setName("");
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not create the token.");
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (t: ApiToken) => {
    try {
      await api.revokeToken(t.id);
      toast.success(`Revoked ${t.name}.`);
      if (minted?.token.id === t.id) setMinted(null);
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not revoke the token.");
    }
  };

  const copy = async () => {
    if (!minted) return;
    await navigator.clipboard.writeText(minted.plaintext);
    setCopied(true);
  };

  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>API tokens</CardTitle>
        <CardDescription>
          Long-lived credentials for scripts, Prometheus and anything else that cannot hold a
          session. Send one as
          <code className="mx-1 font-mono">Authorization: Bearer pmk_…</code>. A token can do
          everything the admin can, except manage tokens — that stays behind the password, so
          revoking one is final.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <form onSubmit={create} className="flex flex-wrap items-end gap-2">
          <div className="flex min-w-48 flex-1 flex-col gap-1">
            <Label htmlFor="tok-name">Name</Label>
            <Input
              id="tok-name"
              value={name}
              maxLength={64}
              placeholder="prometheus"
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <Button type="submit" size="sm" disabled={busy || !name.trim()}>
            {busy ? <Loader2 className="animate-spin" /> : <KeyRound />} Create token
          </Button>
        </form>

        {minted && (
          <div className="flex flex-col gap-1.5 rounded border border-warn/50 bg-warn/5 p-2">
            <span className="text-[11px] font-medium">
              Copy {minted.token.name} now — it is never shown again.
            </span>
            <div className="flex items-center gap-1.5">
              <code className="min-w-0 flex-1 overflow-x-auto rounded border border-border bg-background px-2 py-1 font-mono text-[11px]">
                {minted.plaintext}
              </code>
              <Button size="sm" variant="outline" onClick={copy}>
                {copied ? <Check /> : <Copy />} {copied ? "Copied" : "Copy"}
              </Button>
            </div>
            <span className="text-[10px] text-muted-foreground">
              polyemesis stores only a hash. If you lose it, revoke this token and create another.
            </span>
          </div>
        )}

        {tokens.length === 0 ? (
          <span className="text-[11px] text-muted-foreground">No tokens yet.</span>
        ) : (
          <div className="flex flex-col gap-1">
            {tokens.map((t) => (
              <div
                key={t.id}
                className="flex items-center justify-between gap-2 rounded border border-border bg-background px-2 py-1.5"
              >
                <div className="min-w-0">
                  <div className="truncate text-[12px]">{t.name}</div>
                  <div className="font-mono text-[10px] text-muted-foreground">
                    {t.prefix}… · created {new Date(t.createdAt).toLocaleDateString()} ·{" "}
                    {tokenLastUsed(t)}
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => revoke(t)}
                  aria-label={`Revoke ${t.name}`}
                >
                  <Trash2 />
                </Button>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** The server sends the zero time for a token nothing has presented yet. */
function tokenLastUsed(t: ApiToken): string {
  const used = new Date(t.lastUsedAt);
  if (Number.isNaN(used.getTime()) || used.getUTCFullYear() <= 1) return "never used";
  return `last used ${used.toLocaleString()}`;
}
