import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ExternalLink, Loader2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import type { Destination, DestKind, Platform, PlatformAccount } from "@/lib/types";

/** Per-platform defaults so the common case is one field, not four.
 *  Kick appears here rather than in the OAuth flow because its public API
 *  does not expose stream keys — see internal/oauth/oauth.go. */
const PLATFORM_PRESETS: Record<
  Platform,
  { label: string; kind: DestKind; url: string; hint: string; oauth: boolean }
> = {
  custom: {
    label: "Custom",
    kind: "rtmp",
    url: "",
    hint: "Any RTMP(S) or SRT endpoint.",
    oauth: false,
  },
  youtube: {
    label: "YouTube",
    kind: "rtmp",
    url: "rtmp://a.rtmp.youtube.com/live2",
    hint: "Connect a Google account to fetch the ingest URL and key automatically.",
    oauth: true,
  },
  twitch: {
    label: "Twitch",
    kind: "rtmp",
    url: "rtmp://live.twitch.tv/app",
    hint: "Connect a Twitch account to fetch your stream key automatically.",
    oauth: true,
  },
  kick: {
    label: "Kick",
    kind: "rtmp",
    url: "rtmps://fa723fc1b171.global-contribute.live-video.net",
    hint: "Kick does not expose stream keys over its API. Paste the URL and key from your Kick creator dashboard (Settings → Stream).",
    oauth: false,
  },
};

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Existing destination to edit, or null to create. */
  destination: Destination | null;
  onSaved: () => void;
}

export function DestinationDialog({ open, onOpenChange, destination, onSaved }: Props) {
  const editing = destination !== null;

  const [name, setName] = useState("");
  const [platform, setPlatform] = useState<Platform>("custom");
  const [kind, setKind] = useState<DestKind>("rtmp");
  const [url, setUrl] = useState("");
  const [streamKey, setStreamKey] = useState("");
  const [bitrate, setBitrate] = useState(160);
  const [accountId, setAccountId] = useState<string>("none");
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    api.listAccounts().then(setAccounts).catch(() => setAccounts([]));

    if (destination) {
      setName(destination.name);
      setPlatform(destination.platform);
      setKind(destination.kind);
      setUrl(destination.url);
      setStreamKey(destination.streamKey);
      setBitrate(destination.audioBitrate);
      setAccountId(destination.accountId ? String(destination.accountId) : "none");
    } else {
      setName("");
      setPlatform("custom");
      setKind("rtmp");
      setUrl("");
      setStreamKey("");
      setBitrate(160);
      setAccountId("none");
    }
  }, [open, destination]);

  const preset = PLATFORM_PRESETS[platform];
  const platformAccounts = useMemo(
    () => accounts.filter((a) => a.platform === platform),
    [accounts, platform],
  );

  const changePlatform = (p: Platform) => {
    setPlatform(p);
    const next = PLATFORM_PRESETS[p];
    setKind(next.kind);
    // Only overwrite a URL the user has not customised, so switching platform
    // by accident does not silently discard a pasted endpoint.
    if (!url || Object.values(PLATFORM_PRESETS).some((x) => x.url === url)) {
      setUrl(next.url);
    }
    setAccountId("none");
  };

  const save = async () => {
    setBusy(true);
    try {
      const payload: Partial<Destination> = {
        name: name.trim(),
        kind,
        platform,
        url: url.trim(),
        streamKey: streamKey.trim(),
        audioBitrate: bitrate,
        accountId: accountId === "none" ? null : Number(accountId),
      };
      if (editing) {
        await api.updateDestination(destination.id, payload);
        toast.success("Destination updated.");
      } else {
        await api.createDestination(payload);
        toast.success("Destination created. Set its audio routing next.");
      }
      onSaved();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the destination.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{editing ? "Edit destination" : "Add destination"}</DialogTitle>
          <DialogDescription>
            Video is always passed through untouched. Only audio is re-encoded, using this
            destination's own routing profile.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-name">Name</Label>
            <Input
              id="dest-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="YouTube — main channel"
            />
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label>Platform</Label>
              <Select value={platform} onValueChange={(v) => changePlatform(v as Platform)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(PLATFORM_PRESETS) as Platform[]).map((p) => (
                    <SelectItem key={p} value={p}>
                      {PLATFORM_PRESETS[p].label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="flex flex-col gap-1">
              <Label>Transport</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as DestKind)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="rtmp">RTMP / RTMPS</SelectItem>
                  <SelectItem value="srt">SRT</SelectItem>
                  <SelectItem value="file">Local file</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {preset.oauth && (
            <div className="flex flex-col gap-1">
              <Label>Connected account</Label>
              {platformAccounts.length > 0 ? (
                <Select value={accountId} onValueChange={setAccountId}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">Not linked (enter key manually)</SelectItem>
                    {platformAccounts.map((a) => (
                      <SelectItem key={a.id} value={String(a.id)}>
                        {a.accountName}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Button variant="outline" size="sm" asChild>
                  <a href={api.connectUrl(platform)}>
                    <ExternalLink /> Connect {preset.label} account
                  </a>
                </Button>
              )}
              <span className="text-[10px] text-muted-foreground">
                Linking an account lets polyemesis fetch the stream key for you. Requires
                developer credentials in Settings → Platform credentials.
              </span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-url">
              {kind === "file" ? "Filename" : kind === "srt" ? "SRT URL" : "RTMP URL"}
            </Label>
            <Input
              id="dest-url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className="font-mono"
              placeholder={
                kind === "file"
                  ? "clean-mix.mkv"
                  : kind === "srt"
                    ? "srt://host:9000?streamid=..."
                    : "rtmp://host/app"
              }
            />
            {kind === "file" && (
              <span className="text-[10px] text-muted-foreground">
                Written into the recordings directory. A relative name only — no paths.
              </span>
            )}
          </div>

          {kind === "rtmp" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-key">Stream key</Label>
              <Input
                id="dest-key"
                type="password"
                value={streamKey}
                onChange={(e) => setStreamKey(e.target.value)}
                className="font-mono"
                placeholder="xxxx-xxxx-xxxx-xxxx"
              />
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-bitrate">Audio bitrate</Label>
            <div className="flex items-center gap-2">
              <Input
                id="dest-bitrate"
                type="number"
                min={32}
                max={512}
                value={bitrate}
                onChange={(e) => setBitrate(Number(e.target.value))}
                className="w-24"
              />
              <span className="text-[11px] text-muted-foreground">kbps, AAC stereo</span>
              <Badge variant="outline" className="ml-auto">
                video: copy
              </Badge>
            </div>
          </div>

          <p className="text-[10px] text-muted-foreground">{preset.hint}</p>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={save} disabled={busy || !name.trim()}>
            {busy && <Loader2 className="animate-spin" />}
            {editing ? "Save" : "Create"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
