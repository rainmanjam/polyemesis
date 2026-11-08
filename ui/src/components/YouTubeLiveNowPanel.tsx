import { useEffect, useState } from "react";

import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type {
  DestinationId,
  YouTubeBroadcastControls,
  YouTubeStreamRow,
} from "@/lib/types";

type Streams =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: YouTubeStreamRow[] };

/** The YouTube go-live section of the destination dialog: which of the channel's reusable
 *  streams the destination's broadcast is bound to.
 *
 *  The default, "the stream matching my current key", stores nothing. Choosing a stream stores its
 *  id (youtube.streamId) and nothing else: the list never carries a key, and polyemesis never
 *  changes the key a destination publishes with. A stream another destination already uses is
 *  said so, because YouTube allows only a few live broadcasts per key. */
export function YouTubeLiveNowPanel({
  accountId,
  destinationId,
  controls,
  onChange,
}: Readonly<{
  accountId: number;
  destinationId?: DestinationId;
  controls?: YouTubeBroadcastControls | null;
  onChange: (patch: Partial<YouTubeBroadcastControls>) => void;
}>) {
  const t = useT();
  const [streams, setStreams] = useState<Streams>({ kind: "loading" });

  useEffect(() => {
    let live = true;
    setStreams({ kind: "loading" });
    api
      .youtubeStreams(accountId, destinationId)
      .then((r) => {
        if (live) setStreams({ kind: "ready", rows: r.streams ?? [] });
      })
      .catch((err: unknown) => {
        if (live)
          setStreams({
            kind: "error",
            message: err instanceof Error ? err.message : "",
          });
      });
    return () => {
      live = false;
    };
  }, [accountId, destinationId]);

  const pinned = controls?.streamId ?? "";
  const rows = streams.kind === "ready" ? streams.rows : [];
  const chosen = rows.find((r) => r.id === pinned);
  const others = chosen?.usedBy ?? [];

  const studio = controls?.studioStreamNow ?? false;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="yt-studio-now">{t("yt.live.studioSwitch")}</Label>
          <Switch
            id="yt-studio-now"
            checked={studio}
            onCheckedChange={(v) =>
              onChange({ studioStreamNow: v ? true : undefined })
            }
          />
        </div>
        <span className="text-sm text-muted-foreground">
          {t("yt.live.studioHelp")}
        </span>
        {!studio && (
          <span className="text-sm text-muted-foreground">
            {t("yt.live.privacyNote")}
          </span>
        )}
      </div>
      {!studio && (
        <div className="flex flex-col gap-1">
          <Label htmlFor="yt-stream">{t("yt.live.streamLabel")}</Label>
          <select
            id="yt-stream"
            className="h-9 rounded-md border border-input bg-transparent px-2 text-sm"
            value={pinned}
            disabled={streams.kind !== "ready"}
            onChange={(e) =>
              onChange({ streamId: e.target.value || undefined })
            }
          >
            <option value="">{t("yt.live.streamDefault")}</option>
            {rows.map((r) => (
              <option key={r.id} value={r.id}>
                {t("yt.live.streamOption", {
                  title: r.title || r.id,
                  status: r.streamStatus ?? "-",
                  health: r.health ?? "-",
                })}
              </option>
            ))}
          </select>
          {streams.kind === "loading" && (
            <span className="text-sm text-muted-foreground">
              {t("yt.live.streamLoading")}
            </span>
          )}
          {streams.kind === "error" && (
            <span className="text-sm text-destructive">
              {t("yt.live.streamError", { message: streams.message })}
            </span>
          )}
          {others.length > 0 && (
            <span className="text-sm text-warn">
              {t("yt.live.streamUsedBy", {
                names: others.map((o) => o.name).join(", "),
              })}
            </span>
          )}
          <span className="text-sm text-muted-foreground">
            {t("yt.live.streamHelp")}
          </span>
        </div>
      )}
    </div>
  );
}
