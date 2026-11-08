import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type {
  DestinationId,
  YouTubeBroadcastControls,
  YouTubeEventRow,
  YouTubeStreamRow,
} from "@/lib/types";

type Loaded<T> = { kind: "loading" } | { kind: "error"; message: string } | { kind: "ready"; rows: T[] };

/** Loads a list the server answers as { supported, <key>: rows }, once per account and destination. */
function useList<T>(load: () => Promise<T[]>, deps: unknown[]): [Loaded<T>, () => void] {
  const [state, setState] = useState<Loaded<T>>({ kind: "loading" });
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    setState({ kind: "loading" });
    load()
      .then((rows) => {
        if (live) setState({ kind: "ready", rows });
      })
      .catch((err: unknown) => {
        if (live) setState({ kind: "error", message: err instanceof Error ? err.message : "" });
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  return [state, () => setTick((n) => n + 1)];
}

/** The YouTube go-live section of the destination dialog.
 *
 *  Three choices live here, kept apart: whether polyemesis makes a broadcast at all (the Studio Stream now
 *  switch), whether it makes a new one or uses an existing event, and which of the channel's reusable
 *  streams it is bound to. Neither list ever carries a key, and nothing here changes a key.
 *
 *  An existing event keeps its own settings. The panel reports them (auto-start, auto-stop, the stream it is
 *  bound to) and offers one explicit button to turn auto-start on; it never changes a flag by itself. */
export function YouTubeLiveNowPanel({
  accountId,
  destinationId,
  controls,
  onChange,
  title,
  onTitleChange,
}: Readonly<{
  accountId: number;
  destinationId?: DestinationId;
  controls?: YouTubeBroadcastControls | null;
  onChange: (patch: Partial<YouTubeBroadcastControls>) => void;
  title?: string;
  onTitleChange?: (v: string) => void;
}>) {
  const t = useT();
  const studio = controls?.studioStreamNow ?? false;
  const existing = (controls?.eventId ?? "") !== "";
  const [mode, setMode] = useState<"create" | "existing">(existing ? "existing" : "create");

  const [streams] = useList<YouTubeStreamRow>(
    () => api.youtubeStreams(accountId, destinationId).then((r) => r.streams ?? []),
    [accountId, destinationId],
  );
  const [events, reloadEvents] = useList<YouTubeEventRow>(
    () => (mode === "existing" && !studio ? api.youtubeEvents(accountId, destinationId).then((r) => r.events ?? []) : Promise.resolve([])),
    [accountId, destinationId, mode, studio],
  );
  const [autoStartDone, setAutoStartDone] = useState("");
  const [autoStartError, setAutoStartError] = useState("");

  const pinned = controls?.streamId ?? "";
  const streamRows = streams.kind === "ready" ? streams.rows : [];
  const others = streamRows.find((r) => r.id === pinned)?.usedBy ?? [];

  const eventRows = events.kind === "ready" ? events.rows : [];
  const picked = eventRows.find((e) => e.id === controls?.eventId);

  const chooseMode = (m: "create" | "existing") => {
    setMode(m);
    if (m === "create") onChange({ eventId: undefined, rebindEvent: undefined });
  };

  const turnAutoStartOn = useCallback(
    async (id: string) => {
      setAutoStartError("");
      try {
        await api.youtubeEventAutoStart(accountId, id);
        setAutoStartDone(id);
        reloadEvents();
      } catch (err) {
        setAutoStartError(err instanceof Error ? err.message : "");
      }
    },
    [accountId, reloadEvents],
  );

  const field = "h-9 rounded-md border border-input bg-transparent px-2 text-sm";

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="yt-studio-now">{t("yt.live.studioSwitch")}</Label>
          <Switch
            id="yt-studio-now"
            checked={studio}
            onCheckedChange={(v) => onChange({ studioStreamNow: v ? true : undefined })}
          />
        </div>
        <span className="text-sm text-muted-foreground">{t("yt.live.studioHelp")}</span>
        {!studio && <span className="text-sm text-muted-foreground">{t("yt.live.privacyNote")}</span>}
      </div>

      {!studio && (
        <>
          <div className="flex flex-col gap-1">
            <Label htmlFor="yt-mode">{t("yt.live.modeLabel")}</Label>
            <select
              id="yt-mode"
              className={field}
              value={mode}
              onChange={(e) => chooseMode(e.target.value as "create" | "existing")}
            >
              <option value="create">{t("yt.live.modeCreate")}</option>
              <option value="existing">{t("yt.live.modeExisting")}</option>
            </select>
          </div>

          {mode === "create" && (
            <>
              {onTitleChange && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="yt-title">{t("yt.live.titleLabel")}</Label>
                  <Input
                    id="yt-title"
                    value={title ?? ""}
                    maxLength={100}
                    onChange={(e) => onTitleChange(e.target.value)}
                  />
                  <span className="text-sm text-muted-foreground">{t("yt.live.titleHelp")}</span>
                </div>
              )}
              <div className="flex flex-col gap-1">
                <Label htmlFor="yt-desc">{t("yt.live.descriptionLabel")}</Label>
                <textarea
                  id="yt-desc"
                  className="min-h-16 rounded-md border border-input bg-transparent px-2 py-1 text-sm"
                  maxLength={5000}
                  value={controls?.description ?? ""}
                  onChange={(e) => onChange({ description: e.target.value || undefined })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="yt-category">{t("yt.live.categoryLabel")}</Label>
                <Input
                  id="yt-category"
                  value={controls?.category ?? ""}
                  maxLength={60}
                  onChange={(e) => onChange({ category: e.target.value || undefined })}
                />
                <span className="text-sm text-muted-foreground">{t("yt.live.categoryHelp")}</span>
              </div>
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor="yt-360">{t("yt.live.projection360")}</Label>
                <Switch
                  id="yt-360"
                  checked={controls?.projection360 ?? false}
                  onCheckedChange={(v) => onChange({ projection360: v ? true : undefined })}
                />
              </div>
              <span className="text-sm text-muted-foreground" title={t("yt.live.kidsNote")}>
                {t("yt.live.kidsNote")}
              </span>
            </>
          )}

          {mode === "existing" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="yt-event">{t("yt.live.eventLabel")}</Label>
              <select
                id="yt-event"
                className={field}
                disabled={events.kind !== "ready"}
                value={controls?.eventId ?? ""}
                onChange={(e) => onChange({ eventId: e.target.value || undefined, rebindEvent: undefined })}
              >
                <option value="">{t("yt.live.eventNone")}</option>
                {eventRows.map((e) => (
                  <option key={e.id} value={e.id}>
                    {t("yt.live.eventOption", { title: e.title || e.id, status: e.status })}
                  </option>
                ))}
              </select>
              {events.kind === "loading" && (
                <span className="text-sm text-muted-foreground">{t("yt.live.eventLoading")}</span>
              )}
              {events.kind === "error" && (
                <span className="text-sm text-destructive">{t("yt.live.eventError", { message: events.message })}</span>
              )}
              {events.kind === "ready" && eventRows.length === 0 && (
                <span className="text-sm text-muted-foreground">{t("yt.live.eventNone2")}</span>
              )}
              {picked && (
                <div className="flex flex-col gap-1 text-sm">
                  {!picked.enableAutoStart && (
                    <>
                      <span className="text-warn">{t("yt.live.eventNoAutoStart")}</span>
                      {picked.status === "created" || picked.status === "ready" ? (
                        <Button type="button" variant="outline" size="sm" onClick={() => void turnAutoStartOn(picked.id)}>
                          {t("yt.live.eventAutoStartButton")}
                        </Button>
                      ) : null}
                    </>
                  )}
                  {autoStartDone === picked.id && picked.enableAutoStart && (
                    <span className="text-muted-foreground">{t("yt.live.eventAutoStartDone")}</span>
                  )}
                  {autoStartError && <span className="text-destructive">{autoStartError}</span>}
                  {!picked.enableAutoStop && (
                    <span className="text-muted-foreground">{t("yt.live.eventNoAutoStop")}</span>
                  )}
                  {picked.boundStreamId && !picked.onThisStream && picked.streamActive && (
                    <span className="text-warn">{t("yt.live.eventStreamActive")}</span>
                  )}
                  {picked.boundStreamId && !picked.onThisStream && !picked.streamActive && (
                    <label className="flex items-center justify-between gap-2">
                      <span>
                        {t("yt.live.eventOtherStream")} {t("yt.live.eventRebindConfirm")}
                      </span>
                      <Switch
                        checked={controls?.rebindEvent ?? false}
                        onCheckedChange={(v) => onChange({ rebindEvent: v ? true : undefined })}
                        aria-label={t("yt.live.eventRebindConfirm")}
                      />
                    </label>
                  )}
                </div>
              )}
              <span className="text-sm text-muted-foreground">{t("yt.live.eventHelp")}</span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="yt-stream">{t("yt.live.streamLabel")}</Label>
            <select
              id="yt-stream"
              className={field}
              value={pinned}
              disabled={streams.kind !== "ready"}
              onChange={(e) => onChange({ streamId: e.target.value || undefined })}
            >
              <option value="">{t("yt.live.streamDefault")}</option>
              {streamRows.map((r) => (
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
              <span className="text-sm text-muted-foreground">{t("yt.live.streamLoading")}</span>
            )}
            {streams.kind === "error" && (
              <span className="text-sm text-destructive">{t("yt.live.streamError", { message: streams.message })}</span>
            )}
            {others.length > 0 && (
              <span className="text-sm text-warn">
                {t("yt.live.streamUsedBy", { names: others.map((o) => o.name).join(", ") })}
              </span>
            )}
            <span className="text-sm text-muted-foreground">{t("yt.live.streamHelp")}</span>
          </div>
        </>
      )}
    </div>
  );
}
